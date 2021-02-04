<template>
  <div class="content">
    <b-modal :active.sync="isCreateActive" :on-cancel="resetLocalUser" has-modal-card>
      <div class="modal-card" style="width:25em">
        <header class="modal-card-head">
          <p class="modal-card-title">Create a New User</p>
        </header>
        <section class="modal-card-body">
          <b-field label="User Name" 
            :type="{ 'is-danger' : userExists }" 
            :message="{ 'User already exists' : userExists }">
            <b-input type="text" v-model="user.username" minlength="4" maxlength="32" autofocus></b-input>
          </b-field>
          <b-field label="First Name">
            <b-input type="text" v-model="user.first_name"></b-input>
          </b-field>
          <b-field label="Last Name">
            <b-input type="text" v-model="user.last_name"></b-input>
          </b-field>
          <b-field label="Password">
            <b-input type="password" minlength="8" maxlength="32" v-model="user.password"></b-input>
          </b-field>
          <b-field label="Confirm Password">
            <b-input type="password" minlength="8" maxlength="32" v-model="user.confirmPassword"></b-input>
          </b-field>
          <b-field label="Role">
            <b-select v-model="user.role_name" expanded>
              <option v-for="(r, idx) in roles" :key="idx">
                {{ r }}
              </option>
            </b-select>
          </b-field>
          <b-field>
            <b-button type="is-small is-warning" inverted rounded outlined @click="userRoles=true">
              user roles explained
            </b-button>
          </b-field>
          <b-tooltip label="experiment names can include wildcards for broad
                  assignment (ex. *inv*); they can also include `!` to not
                  allow access to a resource (ex. !*inv*)"
                  type="is-light is-right"
                  multilined>
            <b-field label="Experiment Name(s)"></b-field>
            <b-icon icon="question-circle" style="color:#383838"></b-icon>
          </b-tooltip>
          <b-input type="text" v-model="user.experiments"></b-input>
          <br/>
          <b-tooltip label="resource names can include wildcards for broad
                  assignment (ex. *inv*); they can also include `!` to not
                  allow access to a resource (ex. !*inv*)"
                  type="is-light is-right"
                  multilined>
            <b-field label="Resource Name(s)"></b-field>
            <b-icon icon="question-circle" style="color:#383838"></b-icon>
          </b-tooltip>
          <b-input type="text" v-model="user.resource_names"></b-input>
        </section>
        <footer class="modal-card-foot buttons is-right">
          <button class="button is-light" @click="createUser(false)">Create User</button>
        </footer>
      </div>
    </b-modal>
    <b-modal :active.sync="isEditActive" :on-cancel="resetLocalUser" has-modal-card>
      <div class="modal-card" style="width:25em">
        <header class="modal-card-head">
          <p class="modal-card-title">User {{ user.username }}</p>
        </header>
        <section class="modal-card-body">
          <b-field label="First Name">
            <b-input type="text" v-model="user.first_name" autofocus></b-input>
          </b-field>
          <b-field label="Last Name">
            <b-input type="text" v-model="user.last_name"></b-input>
          </b-field>
          <b-field label="Role">
            <b-select v-model="user.role_name" expanded>
              <option v-for="(r, idx) in roles" :key="idx">
                {{ r }}
              </option>
            </b-select>
          </b-field>
          <b-field>
            <b-button type="is-small is-warning" inverted rounded outlined @click="userRoles=true">
              user roles explained
            </b-button>
          </b-field>
          <b-field label="Experiment Name(s)">
            <b-input type="text" v-model="user.experiments"></b-input>
          </b-field>
          <b-field label="Resource Name(s)">
            <b-input type="text" v-model="user.resource_names"></b-input>
          </b-field>
        </section>
        <footer class="modal-card-foot buttons is-right">
          <button class="button is-light" @click="updateUser">Update User</button>
        </footer>
      </div>
    </b-modal>
    <b-modal :active.sync="userRoles" has-modal-card>
      <div class="modal-card" style="width:70em">
        <header class="modal-card-head">
          <p class="modal-card-title">User Roles Explained</p>
        </header>
        <section class="modal-card-body">
          <p class="color-black">
            Global Admin is the administrator level account and has access to
            all capabilities, to include user management. Global Admins also
            have access to all resources. The following table provides a
            high-level overview of all the available roles and their access
            rights. For a more detailed explanation, including additional
            resources and subresources, please refer to the official phenix
            documentation on <a class="color-orange" target="_blank"
            href="https://activeshadow.com/minimega/user-administration/#role">User
            Administration</a>.
          </p>
          <template>
            <b-table :data="aboutRoles" :columns="aboutCol"></b-table>
          </template>
          <div class="color-black">
            Key: E - experiment resource, V - VM resource, U - user resource
          </div>
        </section>
        <footer class="modal-card-foot buttons is-right">
          <button class="button is-light" @click="userRoles = false">Close</button>
        </footer>
      </div>
    </b-modal>
    <template>
      <hr>
      <b-field grouped position="is-right">
        <p v-if="adminUser()" class="control">
          <b-tooltip label="create a new user" type="is-light is-left">
            <button class="button is-light" @click="createUser(true)">
              <b-icon icon="plus"></b-icon>
            </button>
          </b-tooltip>
        </p>
      </b-field>
      <div>
        <b-table
          :data="users"
          :paginated="table.isPaginated && paginationNeeded"
          :per-page="table.perPage"
          :current-page.sync="table.currentPage"
          :pagination-simple="table.isPaginationSimple"
          :pagination-size="table.paginationSize"
          :default-sort-direction="table.defaultSortDirection"
          default-sort="username">
          <template slot-scope="props">
            <b-table-column field="username" label="User" sortable>
              <template v-if="adminUser()">
                <b-tooltip label="change user settings" type="is-dark">
                  <div class="field">
                    <div @click="editUser( props.row.username )">
                      {{ props.row.username }}
                    </div>
                  </div>
                </b-tooltip>
              </template>
              <template v-else>
                {{ props.row.username }}
              </template>
            </b-table-column>
            <b-table-column field="first_name" label="First Name">
              {{ props.row.first_name }}
            </b-table-column>
            <b-table-column field="last_name" label="Last Name" sortable>
              {{ props.row.last_name }}
            </b-table-column>
            <b-table-column field="role" label="Role" sortable>
              <b-tooltip label="show role explanations" type="is-dark">
                <div class="field">
                  <div @click="userRoles=true">
                    {{ props.row.rbac.roleName ? props.row.rbac.roleName : "Not yet assigned" }}
                  </div>
                </div>
              </b-tooltip>
            </b-table-column>
            <b-table-column v-if="adminUser()" label="Delete" width="50" centered>
              <button class="button is-light is-small" @click="deleteUser( props.row.username )">
                <b-icon icon="trash"></b-icon>
              </button>
            </b-table-column>
          </template>
        </b-table>
        <b-field v-if="paginationNeeded" grouped position="is-right">
          <div class="control is-flex">
            <b-switch v-model="table.isPaginated" size="is-small" type="is-light">Pagenate</b-switch>
          </div>
        </b-field>
      </div>
    </template>
    <b-loading :is-full-page="true" :active.sync="isWaiting" :can-cancel="false"></b-loading>
  </div>
</template>

<script>
  export default {
    
    beforeDestroy () {
      this.$options.sockets.onmessage = null;
    },
    
    async created () {
      this.$options.sockets.onmessage = this.handler;
      this.updateUsers();
    },

    computed: {
      paginationNeeded () {
        if ( this.users.length <= 10 ) {
          return false;
        } else {
          return true;
        }
      }
    },

    methods: {
      handler ( event ) {
        event.data.split(/\r?\n/).forEach( m => {
          let msg = JSON.parse( m );
          this.handle( msg );
        });
      },
    
      handle ( msg ) {
        // We only care about publishes pertaining to a user resource.
        if ( msg.resource.type != 'user' ) {
          return;
        }

        let users = this.users;

        switch ( msg.resource.action ) {
          case 'create': {
            let user = msg.result;
            user = this.parseUserRole(user);
            users.push( user );
      
            this.users = [ ...users ];
          
            this.$buefy.toast.open({
              message: 'The ' + msg.resource.name + ' user was created.',
              type: 'is-success'
            });

            break;
          }

          case 'update': {
            for ( let i = 0; i < users.length; i++ ) {
              if ( users[i].username == msg.resource.name ) {
                let user = msg.result;
                user = this.parseUserRole(user);
                users[i] = user;

                break;
              }
            }
          
            this.users = [ ...users ];
          
            this.$buefy.toast.open({
              message: 'The ' + msg.resource.name + ' user was updated.',
              type: 'is-success'
            });

            break;
          }

          case 'delete': {
            for ( let i = 0; i < users.length; i++ ) {
              if ( users[i].username == msg.resource.name ) {
                users.splice( i, 1 );
                break;
              }
            }
          
            this.users = [ ...users ];
          
            this.$buefy.toast.open({
              message: 'The ' + msg.resource.name + ' user was deleted.',
              type: 'is-success'
            });

            break;
          }
        }
      },

      updateUsers () {
        this.$http.get( 'users' ).then(
          response => {
            response.json().then(
              state => {
                let users = [];

                state.users.forEach( u => {
                  u = this.parseUserRole(u);
                  users.push(u);
                });

                this.users = users;
                this.isWaiting = false;
              }
            );
          }, response => {
            this.$buefy.toast.open({
              message: 'Getting the users failed.',
              type: 'is-danger',
              duration: 4000
            });
            
            this.isWaiting = false;
          }
        );
      },
      
      adminUser () {
        return [ 'Global Admin', 'Experiment Admin' ].includes( this.$store.getters.role );
      },
      
      experimentUser () {
        return [ 'Global Admin', 'Experiment Admin', 'Experiment User' ].includes( this.$store.getters.role );
      },

      parseUserRole( user ) {
        if ( user.rbac ) {
          user.role_name = user.rbac.roleName;

          let experiments = [];
          let resource_names = [];

          /* parse policies to get accurate experiments and resource names */
          for ( let i = 0; i < user.rbac.policies.length; i++ ) {
            let policy = user.rbac.policies[i];

            /* not all policies are scoped by experiments */
            if ( policy.experiments ) {
              experiments.push(...policy.experiments);
            }

            /*
             * Don't worry about processing resources if this policy has no
             * resource names associated with it (which should rarely happen, if
             * ever).
             */
            if ( !policy.resourceNames ) {
              continue;
            }

            policy.resources.forEach( r => {
              let tokens = r.split('/');

              switch ( tokens[0] ) {
                case 'vms': {
                  let names = policy.resourceNames.map(n => 'vms/' + n);
                  resource_names.push(...names);
                  break;
                }

                case 'hosts': {
                  let names = policy.resourceNames.map(n => 'hosts/' + n);
                  resource_names.push(...names);
                  break;
                }

                case 'disks': {
                  let names = policy.resourceNames.map(n => 'disks/' + n);
                  resource_names.push(...names);
                  break;
                }
              }
            });
          }

          if ( experiments.length != 0 ) {
            user.experiments = _.uniq( experiments ).join(' ');
          } else {
            /* assume access to all experiments if not specified in policy */
            user.experiments = '*';
          }

          if ( resource_names.length != 0 ) {
            user.resource_names = _.uniq( resource_names ).join(' ');
          } else {
            /* assume access to all resources if not specified in policy */
            user.resource_names = '*';
          }
        }

        return user;
      },

      createUser (init) {
        if ( init ) {
          this.user.experiments = this.user.experiments.join(' ');
          this.user.resource_names = this.user.resource_names.join(' ');

          // this will show create modal
          this.isCreateActive = true;

          return
        }

        for ( let i = 0; i < this.users.length; i++ ) {
          if ( this.users[i].username == this.user.username ) {
            this.userExists = true;
            return;
          }
        }

        if ( !this.user.username ) {
          this.$buefy.toast.open({
            message: 'You must include an username',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        if ( !this.user.first_name ) {
          this.$buefy.toast.open({
            message: 'You must include a first name',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        if ( !this.user.last_name ) {
          this.$buefy.toast.open({
            message: 'You must include a last name',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        if ( !this.user.password ) {
          this.$buefy.toast.open({
            message: 'You must include a password',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        if ( !this.user.confirmPassword ) {
          this.$buefy.toast.open({
            message: 'You must include a password confirmation',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        if ( this.user.password != this.user.confirmPassword ) {
          this.$buefy.toast.open({
            message: 'Your passwords do not match',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        delete this.user.confirmPassword;

        if ( !this.user.role_name ) {
          this.$buefy.toast.open({
            message: 'You must select a role',
            type: 'is-warning',
            duration: 4000
          });

          return;
        }

        if ( this.user.experiments ) {
          this.user.experiments = this.user.experiments.split(/\s+/);
        }

        if ( this.user.resource_names ) {
          this.user.resource_names = this.user.resource_names.split(/\s+/);
        }

        this.isWaiting = true;
        
        let name = this.user.username;
        
        this.$http.post(
          'users', this.user
        ).then(
          response => {            
            this.isWaiting = false;
          }, response => {
            this.$buefy.toast.open({
              message: 'Creating the user ' + name + ' failed with ' + response.status + ' status.',
              type: 'is-danger',
              duration: 4000
            });
            
            this.isWaiting = false;
          }
        )

        this.resetLocalUser();
        this.isCreateActive = false;
      },

      editUser ( username ) {
        for ( let i = 0; i < this.users.length; i++ ) {
          if ( this.users[i].username == username ) {
            this.user = this.users[i];
            break;
          }
        }

        this.isEditActive = true;
      },

      updateUser () {
        if ( this.$store.getters.username == this.user.username && this.$store.getters.role != this.user.role_name ) {
          this.$buefy.toast.open({
            message: 'You cannot change the role of the user you are currently logged in as.',
            type: 'is-danger',
            duration: 5000
          });
          
          this.resetLocalUser();
          this.isWaiting = false;
          this.isEditActive = false;
          
          return;
        }
        
        delete this.user.id;
        
        let user = this.user;

        user.experiments = user.experiments.split(/\s+/);
        user.resource_names = user.resource_names.split(/\s+/);
        
        this.isEditActive = false;
        this.isWaiting = true;

        this.$http.patch( 
          'users/' + user.username, user 
        ).then(
          response => {
            let users = this.users;
                  
            for ( let i = 0; i < users.length; i++ ) {
              if ( users[i].username == user.username ) {
                users[i] = user;
                break;
              }
            }
            
            this.users = [ ...users ];       
            this.isWaiting = false;
          }, response => {
            this.$buefy.toast.open({
              message: 'Updating the ' + user.username + ' user failed with ' + response.status + ' status.',
              type: 'is-danger',
              duration: 4000
            });
            
            this.isWaiting = false;
          }
        )

        this.resetLocalUser();
      },

      deleteUser ( username ) {
        if ( username === this.$store.getters.username ) {
          this.$buefy.toast.open({
            message: 'You cannot delete the user you are currently logged in as.',
            type: 'is-danger',
            duration: 4000
          });

          return;
        }

        this.$buefy.dialog.confirm({
          title: 'Delete the User',
          message: 'This will DELETE the ' + username + ' user. Are you sure you want to do this?',
          cancelText: 'Cancel',
          confirmText: 'Delete',
          type: 'is-danger',
          hasIcon: true,
          onConfirm: () => {
            this.isWaiting = true;
            
            this.$http.delete(
              'users/' + username
            ).then(
              response => {
                let users = this.users;
                  
                for ( let i = 0; i < users.length; i++ ) {
                  if ( users[i].username == username ) {
                    users.splice( i, 1 );
                    break;
                  }
                }
                
                this.users = [ ...users ];
            
                this.isWaiting = false;
              }, response => {
                this.$buefy.toast.open({
                  message: 'Deleting the user ' + username + ' failed with ' + response.status + ' status.',
                  type: 'is-danger',
                  duration: 4000
                });
              }
            )
          }
        })
      },

      resetLocalUser () {
        this.user = {
          'experiments': ['*'],
          'resource_names': ['vms/*', 'hosts/*', 'disks/*']
        };

      }
    },

    data () {
      return {
        table: {
          striped: true,
          isPaginated: true,
          isPaginationSimple: true,
          paginationSize: 'is-small',
          defaultSortDirection: 'asc',
          currentPage: 1,
          perPage: 10
        },
        roles: [
          'Global Admin',
          'Global Viewer',
          'Experiment Admin',
          'Experiment User',
          'Experiment Viewer',
          'VM Viewer'
        ],
        users: [],
        user: {
          'experiments': ['*'],
          'resource_names': ['vms/*', 'hosts/*', 'disks/*']
        },
        aboutRoles: [
          { 'role': 'Global Admin', 'limits': 'Can see and control absolutely anything/everything.', 'list': 'E V U', 'get': 'E V U', 'create': 'E V U', 'update': 'E V U', 'patch': 'E V U', 'delete': 'E V U' },
          { 'role': 'Global Viewer', 'limits': 'Can see absolutely anything/everything, but cannot make any changes.', 'list': 'E V U', 'get': 'E V U', 'create': '', 'update': '', 'patch': '', 'delete': '' },
          { 'role': 'Experiment Admin', 'limits': 'Can see and control anything/everything for assigned experiments, including VMs, but cannot create new experiments.', 'list': 'E V', 'get': 'E V', 'create': 'V', 'update': 'E V', 'patch': 'V', 'delete': 'V' },
          { 'role': 'Experiment User', 'limits': 'Can see assigned experiments, and can control VMs within assigned experiments, but cannot modify experiments themselves.', 'list': 'E V', 'get': 'E V', 'create': '', 'update': '', 'patch': 'V', 'delete': '' },
          { 'role': 'Experiment Viewer', 'limits': 'Can see assigned experiments and VMs within assigned experiments, but cannot modify or control experiments or VMs.', 'list': 'E V', 'get': 'E V', 'create': '', 'update': '', 'patch': '', 'delete': '' },
          { 'role': 'VM Viewer', 'limits': 'Can only see VM screenshots and access VM VNC, nothing else.', 'list': '', 'get': 'V', 'create': '', 'update': '', 'patch': '', 'delete': '' }
        ],
        aboutCol: [
          { field: 'role', label: 'Role' },
          { field: 'limits', label: 'Limits' },
          { field: 'list', label: 'List', width: '75' },
          { field: 'get', label: 'Get', width: '75' },
          { field: 'create', label: 'Create' },
          { field: 'update', label: 'Update' },
          { field: 'patch', label: 'Patch' },
          { field: 'delete', label: 'Delete' }
        ],
        userExists: false,
        isCreateActive: false,
        isEditActive: false,
        isWaiting: true,
        userRoles: false
      }
    }
  }
</script>

<style scoped>
ul {
  columns: 2;
  -webkit-columns: 2;
  -moz-columns: 2;
}

li {
  color: black !important;
}

.color-black {
  color: black !important;
}

a.color-orange:link {
  color: #CC5500 !important;
}

a.color-orange:visited {
  color: #CC5500 !important;
}

a.color-orange:hover {
  color: #CC5500 !important;
}
</style>
